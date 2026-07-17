import { type FormEvent, type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { AppShell } from '../components/AppShell'
import {
  dataSourceAPI,
  type DataSourceConnectionInput,
  type DataSourceColumnRecord,
  type DataSourceRecord,
  type DataSourceStatus,
  type DataSourceTableRecord,
  type DataSourceType,
  type DiscoveredTableRecord,
} from '../lib/data-sources'

const statusLabels: Record<DataSourceStatus, string> = {
  DRAFT: '待验证', ACTIVE: '运行中', DISABLED: '已暂停', SYNCING: '同步中', ERROR: '异常', DELETING: '删除中',
}
const typeLabels: Record<DataSourceType, string> = { MYSQL: 'MySQL', ORACLE: 'Oracle', EXCEL: 'Excel / CSV' }
type DatabaseType = DataSourceConnectionInput['type']
type ConnectionDraft = Omit<DataSourceConnectionInput, 'port' | 'config'> & { port: string }
type DialogState = { mode: 'create' | 'view' | 'edit' | 'delete' | 'select-tables' | 'edit-table' | 'delete-table'; source?: DataSourceRecord; table?: DataSourceTableRecord }
type Notice = { tone: 'success' | 'error'; message: string }
type TableDraft = { businessName: string; businessDescription: string; tags: string; sensitivityLevel: string; visibility: string; manualLocked: boolean }

const emptyDraft = (): ConnectionDraft => ({ code: '', name: '', type: 'MYSQL', host: '', port: '3306', database: '', username: '', password: '' })
const configText = (source: DataSourceRecord, key: string) => {
  const value = source.config?.[key]
  return typeof value === 'string' || typeof value === 'number' ? String(value) : ''
}
const discoveredTableKey = (table: DiscoveredTableRecord) => `${table.catalogName}\u001f${table.schemaName}\u001f${table.name}`
const assetTableKey = (table: DataSourceTableRecord) => `${table.catalogName}\u001f${table.schemaName}\u001f${table.tableName}`
const draftFromSource = (source: DataSourceRecord): ConnectionDraft => ({
  code: source.code,
  name: source.name,
  type: source.type === 'ORACLE' ? 'ORACLE' : 'MYSQL',
  host: configText(source, 'host'),
  port: configText(source, 'port') || (source.type === 'ORACLE' ? '1521' : '3306'),
  database: configText(source, 'database'),
  username: configText(source, 'username'),
  password: '',
})

/** 提供数据源目录、结构化连接配置和完整生命周期操作，浏览器永不接收已保存密码。 */
export function DataSourceCenterPage() {
  const [sources, setSources] = useState<DataSourceRecord[]>([])
  const [loading, setLoading] = useState(true)
  const [notice, setNotice] = useState<Notice | null>(null)
  const [dialog, setDialog] = useState<DialogState | null>(null)
  const [draft, setDraft] = useState<ConnectionDraft>(emptyDraft)
  const [busyAction, setBusyAction] = useState('')
  const [formError, setFormError] = useState('')
  const [keyword, setKeyword] = useState('')
  const [typeFilter, setTypeFilter] = useState<DataSourceType | 'ALL'>('ALL')
  const [statusFilter, setStatusFilter] = useState<DataSourceStatus | 'ALL'>('ALL')
  const [metadataTables, setMetadataTables] = useState<DataSourceTableRecord[]>([])
  const [metadataColumns, setMetadataColumns] = useState<Record<string, DataSourceColumnRecord[]>>({})
  const [metadataLoading, setMetadataLoading] = useState(false)
  const [metadataError, setMetadataError] = useState('')
  const [columnLoading, setColumnLoading] = useState<Record<string, boolean>>({})
  const [discoveredTables, setDiscoveredTables] = useState<DiscoveredTableRecord[]>([])
  const [selectedTableKeys, setSelectedTableKeys] = useState<string[]>([])
  const [discoveryLoading, setDiscoveryLoading] = useState(false)
  const [tableDraft, setTableDraft] = useState<TableDraft>({ businessName: '', businessDescription: '', tags: '', sensitivityLevel: 'INTERNAL', visibility: 'PRIVATE', manualLocked: false })
  const metadataRequest = useRef(0)

  useEffect(() => {
    if (!notice) return
    const timeout = window.setTimeout(() => setNotice(null), 4500)
    return () => window.clearTimeout(timeout)
  }, [notice])

  const filteredSources = useMemo(() => {
    const query = keyword.trim().toLocaleLowerCase()
    return sources.filter(source => {
      const matchesKeyword = !query || source.name.toLocaleLowerCase().includes(query) || source.code.toLocaleLowerCase().includes(query)
      return matchesKeyword && (typeFilter === 'ALL' || source.type === typeFilter) && (statusFilter === 'ALL' || source.status === statusFilter)
    })
  }, [keyword, sources, statusFilter, typeFilter])

  const loadSources = useCallback(async () => {
    try {
      const page = await dataSourceAPI.list()
      setSources(page.items)
      return page.items
    } catch (cause) {
      setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '加载数据源失败' })
      return null
    } finally {
      setLoading(false)
    }
  }, [])

  const loadTableStructures = useCallback(async (sourceId: string) => {
    const request = ++metadataRequest.current
    setMetadataLoading(true)
    setMetadataError('')
    setMetadataTables([])
    setMetadataColumns({})
    setColumnLoading({})
    try {
      const result = await dataSourceAPI.tables(sourceId)
      if (request === metadataRequest.current) setMetadataTables(result.items)
    } catch (cause) {
      if (request === metadataRequest.current) setMetadataError(cause instanceof Error ? cause.message : '加载表结构失败')
    } finally {
      if (request === metadataRequest.current) setMetadataLoading(false)
    }
  }, [])

  const loadColumns = async (table: DataSourceTableRecord) => {
    if (metadataColumns[table.id] || columnLoading[table.id]) return
    setColumnLoading(current => ({ ...current, [table.id]: true }))
    try {
      const result = await dataSourceAPI.columns(table.id)
      setMetadataColumns(current => ({ ...current, [table.id]: result.items.filter(column => column.assetStatus === 'ACTIVE') }))
    } catch (cause) {
      setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : `加载表 ${table.tableName} 的字段失败` })
    } finally {
      setColumnLoading(current => ({ ...current, [table.id]: false }))
    }
  }

  useEffect(() => {
    let active = true
    dataSourceAPI.list().then(page => {
      if (!active) return
      setSources(page.items)
    }).catch(cause => {
      if (active) setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '加载数据源失败' })
    }).finally(() => {
      if (active) setLoading(false)
    })
    return () => { active = false }
  }, [])

  const openCreate = () => {
    setDraft(emptyDraft())
    setFormError('')
    setDialog({ mode: 'create' })
  }
  const openExisting = (mode: DialogState['mode'], source: DataSourceRecord) => {
    setFormError('')
    setDraft(draftFromSource(source))
    setDialog({ mode, source })
    if (mode === 'view') void loadTableStructures(source.id)
  }
  const openTableSelection = async (source: DataSourceRecord) => {
    setDialog({ mode: 'select-tables', source })
    setDiscoveredTables([])
    setSelectedTableKeys([])
    setDiscoveryLoading(true)
    setFormError('')
    try {
      const result = await dataSourceAPI.discoverTables(source.id)
      setDiscoveredTables(result.items)
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : '读取源库数据表失败')
    } finally {
      setDiscoveryLoading(false)
    }
  }
  const openTableEditor = (source: DataSourceRecord, table: DataSourceTableRecord) => {
    setTableDraft({ businessName: table.businessName, businessDescription: table.businessDescription, tags: table.tags.join(', '), sensitivityLevel: table.sensitivityLevel, visibility: table.visibility, manualLocked: table.manualLocked })
    setFormError('')
    setDialog({ mode: 'edit-table', source, table })
  }
  const closeDialog = () => {
    if (!busyAction) {
      metadataRequest.current += 1
      setDialog(null)
    }
  }
  const updateDraft = (key: keyof ConnectionDraft, value: string) => setDraft(current => ({ ...current, [key]: value }))

  const importSelectedTables = async () => {
    const source = dialog?.source
    if (!source || selectedTableKeys.length === 0) {
      setFormError('请至少选择一张数据表')
      return
    }
    const selected = new Set(selectedTableKeys)
    const tables = discoveredTables.filter(table => selected.has(discoveredTableKey(table))).map(table => ({ catalogName: table.catalogName, schemaName: table.schemaName, tableName: table.name }))
    setBusyAction('import-tables')
    setFormError('')
    try {
      await dataSourceAPI.importTables(source.id, tables)
      setNotice({ tone: 'success', message: `已完成 ${tables.length} 张表的采样、LLM 完善和资产入库` })
      setDialog({ mode: 'view', source })
      await loadTableStructures(source.id)
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : '新增数据表资产失败')
    } finally {
      setBusyAction('')
    }
  }

  const updateTableAsset = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const source = dialog?.source
    const table = dialog?.table
    if (!source || !table || !tableDraft.businessName.trim()) {
      setFormError('请填写数据表业务名称')
      return
    }
    setBusyAction(`edit-table:${table.id}`)
    try {
      await dataSourceAPI.updateTable(table.id, {
        businessName: tableDraft.businessName.trim(), businessDescription: tableDraft.businessDescription.trim(),
        tags: tableDraft.tags.split(',').map(tag => tag.trim()).filter(Boolean), sensitivityLevel: tableDraft.sensitivityLevel,
        visibility: tableDraft.visibility, manualLocked: tableDraft.manualLocked, expectedVersion: table.businessVersion,
      })
      setNotice({ tone: 'success', message: `已修改表资产“${table.businessName || table.tableName}”` })
      setDialog({ mode: 'view', source })
      await loadTableStructures(source.id)
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : '修改数据表资产失败')
    } finally {
      setBusyAction('')
    }
  }

  const changeTableStatus = async (source: DataSourceRecord, table: DataSourceTableRecord) => {
    const enable = table.managementStatus === 'DISABLED'
    setBusyAction(`table-status:${table.id}`)
    try {
      if (enable) await dataSourceAPI.enableTable(table.id)
      else await dataSourceAPI.disableTable(table.id)
      setNotice({ tone: 'success', message: `已${enable ? '恢复' : '停用'}表资产“${table.businessName || table.tableName}”` })
      await loadTableStructures(source.id)
    } catch (cause) {
      setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '更新表资产状态失败' })
    } finally {
      setBusyAction('')
    }
  }

  const refreshTableAsset = async (source: DataSourceRecord, table: DataSourceTableRecord) => {
    setBusyAction(`refresh-table:${table.id}`)
    try {
      await dataSourceAPI.importTables(source.id, [{ catalogName: table.catalogName, schemaName: table.schemaName, tableName: table.tableName }])
      setNotice({ tone: 'success', message: `已刷新并重新完善表资产“${table.businessName || table.tableName}”` })
      await loadTableStructures(source.id)
    } catch (cause) {
      setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '刷新表结构失败' })
    } finally {
      setBusyAction('')
    }
  }

  const refreshAllTableAssets = async (source: DataSourceRecord) => {
    setBusyAction(`refresh-tables:${source.id}`)
    try {
      const result = await dataSourceAPI.refreshTables(source.id)
      if (result.total === 0) setNotice({ tone: 'success', message: '当前没有可刷新的数据表资产' })
      else if (result.status === 'SUCCEEDED') setNotice({ tone: 'success', message: `已刷新 ${result.succeeded} 张表的结构并重新完成 LLM 加工` })
      else setNotice({ tone: 'error', message: `元数据刷新完成：${result.succeeded} 张成功，${result.failed} 张失败` })
      await loadTableStructures(source.id)
    } catch (cause) {
      setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '刷新全部元数据失败' })
    } finally {
      setBusyAction('')
    }
  }

  const deleteTableAsset = async () => {
    const source = dialog?.source
    const table = dialog?.table
    if (!source || !table) return
    setBusyAction(`delete-table:${table.id}`)
    try {
      await dataSourceAPI.deleteTable(table.id)
      setNotice({ tone: 'success', message: `已从 PostgreSQL 删除表资产“${table.businessName || table.tableName}”，源库原表未受影响` })
      setDialog({ mode: 'view', source })
      await loadTableStructures(source.id)
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : '删除数据表资产失败')
    } finally {
      setBusyAction('')
    }
  }

  const submitConnection = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const port = Number(draft.port)
    const editing = dialog?.mode === 'edit' && dialog.source
    if (!draft.name.trim() || !draft.code.trim() || !draft.host.trim() || !draft.database.trim() || !draft.username.trim() || !Number.isInteger(port) || port < 1 || port > 65535 || (!editing && !draft.password)) {
      setFormError(`请完整填写连接信息${editing ? '' : '和密码'}，端口需为 1–65535 的整数`)
      return
    }
    const input: DataSourceConnectionInput = {
      code: draft.code.trim(), name: draft.name.trim(), type: draft.type, host: draft.host.trim(), port,
      database: draft.database.trim(), username: draft.username.trim(), password: draft.password,
    }
    setBusyAction(editing ? `edit:${editing.id}` : 'create')
    setFormError('')
    try {
      const saved = editing ? await dataSourceAPI.update(editing.id, input) : await dataSourceAPI.create(input)
      setSources(current => editing ? current.map(source => source.id === saved.id ? saved : source) : [saved, ...current])
      setNotice({ tone: 'success', message: editing ? `已更新“${saved.name}”，请重新测试连接后启用` : `已创建“${saved.name}”` })
      setDialog(null)
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : editing ? '更新数据源失败' : '新建数据源失败')
    } finally {
      setBusyAction('')
    }
  }

  const changeStatus = async (source: DataSourceRecord) => {
    const resume = source.status === 'DISABLED'
    setBusyAction(`status:${source.id}`)
    setNotice(null)
    try {
      if (resume) await dataSourceAPI.enable(source.id)
      else await dataSourceAPI.disable(source.id)
      const latest = await loadSources()
      const updated = latest?.find(item => item.id === source.id)
      if (updated) setDialog(current => current?.mode === 'view' && current.source?.id === updated.id ? { ...current, source: updated } : current)
      setNotice({ tone: 'success', message: `已${resume ? '恢复' : '暂停'}“${source.name}”` })
    } catch (cause) {
      setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : `${resume ? '恢复' : '暂停'}数据源失败` })
    } finally {
      setBusyAction('')
    }
  }

  const testConnection = async (source: DataSourceRecord) => {
    setBusyAction(`test:${source.id}`)
    setNotice(null)
    try {
      const result = await dataSourceAPI.test(source.id)
      const latest = await loadSources()
      const updated = latest?.find(item => item.id === source.id)
      if (updated) setDialog(current => current?.mode === 'view' && current.source?.id === updated.id ? { ...current, source: updated } : current)
      setNotice({ tone: 'success', message: `“${source.name}”连接成功 · ${result.serverVersion || '版本未知'} · ${result.latencyMs} ms` })
    } catch (cause) {
      // 测试失败后服务端会把数据源标记为异常，先刷新状态再保留错误原因。
      await loadSources()
      setNotice({ tone: 'error', message: cause instanceof Error ? cause.message : '测试数据源连接失败' })
    } finally {
      setBusyAction('')
    }
  }

  const deleteSource = async () => {
    const source = dialog?.source
    if (!source) return
    setBusyAction(`delete:${source.id}`)
    setFormError('')
    try {
      await dataSourceAPI.delete(source.id)
      setSources(current => current.filter(item => item.id !== source.id))
      setNotice({ tone: 'success', message: `已删除“${source.name}”` })
      setDialog(null)
    } catch (cause) {
      setFormError(cause instanceof Error ? cause.message : '删除数据源失败')
    } finally {
      setBusyAction('')
    }
  }

  const actionBusy = Boolean(busyAction)
  return (
    <AppShell title="数据源配置中心" eyebrow="工作栏" actions={<button className="primary-button" type="button" disabled={actionBusy} onClick={openCreate}>新建数据源</button>}>
      {notice && <div className={`data-source-toast ${notice.tone}`} role={notice.tone === 'error' ? 'alert' : 'status'} aria-live="polite">
        <span className="data-source-toast-icon" aria-hidden="true">{notice.tone === 'success' ? '✓' : '!'}</span>
        <span>{notice.message}</span>
        <button type="button" aria-label="关闭消息" onClick={() => setNotice(null)}>×</button>
      </div>}
      <section className="data-source-center" aria-label="数据源配置中心内容">
        <header className="data-source-summary"><div><span className="eyebrow">数据源清单</span><h2>已有的数据源</h2><p>统一查看、修改和管理当前租户的数据连接。</p></div><strong aria-label={`${sources.length} 个数据源`}>{sources.length}<small> 个数据源</small></strong></header>
        <div className="data-source-filters" aria-label="数据源筛选">
          <label><span>搜索</span><input aria-label="搜索数据源" type="search" value={keyword} onChange={event => setKeyword(event.target.value)} placeholder="名称或编码" /></label>
          <label><span>类型</span><select aria-label="按类型筛选" value={typeFilter} onChange={event => setTypeFilter(event.target.value as DataSourceType | 'ALL')}><option value="ALL">全部类型</option><option value="MYSQL">MySQL</option><option value="ORACLE">Oracle</option><option value="EXCEL">Excel / CSV</option></select></label>
          <label><span>状态</span><select aria-label="按状态筛选" value={statusFilter} onChange={event => setStatusFilter(event.target.value as DataSourceStatus | 'ALL')}><option value="ALL">全部状态</option>{Object.entries(statusLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
          <small>显示 {filteredSources.length} / {sources.length}</small>
        </div>
        {loading ? <div className="data-source-empty">正在加载数据源…</div> : sources.length === 0
          ? <div className="data-source-empty"><strong>还没有数据源</strong><span>点击右上角“新建数据源”开始配置。</span></div>
          : filteredSources.length === 0 ? <div className="data-source-empty"><strong>没有符合条件的数据源</strong><span>请调整搜索词或筛选条件。</span></div>
          : <div className="data-source-list" role="list" aria-label="已有数据源清单">{filteredSources.map(source => {
              const canToggle = source.status === 'ACTIVE' || source.status === 'DISABLED'
              const unavailable = source.status === 'SYNCING' || source.status === 'DELETING'
              const canTest = !unavailable && source.status !== 'DISABLED'
              return <article className="data-source-card" role="listitem" key={source.id}>
                <button className="data-source-card-open" type="button" aria-label={`管理${source.name}的数据表资产`} onClick={() => openExisting('view', source)}>
                  <span className={`data-source-icon ${source.type.toLowerCase()}`}>{source.type === 'EXCEL' ? 'XL' : 'DB'}</span>
                  <span className="data-source-main"><span><strong role="heading" aria-level={3}>{source.name}</strong><span className={`data-source-status ${source.status.toLowerCase()}`}>{statusLabels[source.status]}</span></span><span className="data-source-subtitle">{typeLabels[source.type]} · {source.code}</span></span>
                  <span className="data-source-card-facts"><span><small>地址</small><strong>{configText(source, 'host') || '文件数据源'}{configText(source, 'port') ? `:${configText(source, 'port')}` : ''}</strong></span><span><small>数据库</small><strong>{configText(source, 'database') || '—'}</strong></span></span>
                </button>
                <div className="data-source-actions">
                  <button className="action-view" type="button" onClick={event => { event.stopPropagation(); openExisting('view', source) }}>查看</button>
                  <button className="action-edit" type="button" disabled={actionBusy || unavailable || source.type === 'EXCEL'} onClick={event => { event.stopPropagation(); openExisting('edit', source) }}>修改</button>
                  <button className="action-test" type="button" disabled={actionBusy || !canTest} onClick={event => { event.stopPropagation(); void testConnection(source) }}>{busyAction === `test:${source.id}` ? '测试中…' : '测试连接'}</button>
                  <button className={source.status === 'DISABLED' ? 'action-resume' : 'action-pause'} type="button" disabled={actionBusy || !canToggle} onClick={event => { event.stopPropagation(); void changeStatus(source) }}>{source.status === 'DISABLED' ? '恢复' : '暂停'}</button>
                  <button className="action-delete" type="button" disabled={actionBusy || unavailable} onClick={event => { event.stopPropagation(); openExisting('delete', source) }}>删除</button>
                </div>
              </article>
            })}</div>}
      </section>

      {(dialog?.mode === 'create' || dialog?.mode === 'edit') && <Dialog title={dialog.mode === 'edit' ? '修改数据源' : '新建数据源'} onClose={closeDialog}>
        <form className="data-source-form" onSubmit={submitConnection}>
          <div className="data-source-form-grid">
            <label>数据源名称<input autoFocus value={draft.name} onChange={event => updateDraft('name', event.target.value)} placeholder="例如：销售业务库" /></label>
            <label>数据源编码<input value={draft.code} onChange={event => updateDraft('code', event.target.value)} placeholder="例如：sales_mysql" /></label>
            <label>数据源类型<select value={draft.type} onChange={event => {
              const type = event.target.value as DatabaseType
              setDraft(current => ({ ...current, type, port: current.port === '3306' || current.port === '1521' ? (type === 'ORACLE' ? '1521' : '3306') : current.port }))
            }}><option value="MYSQL">MySQL</option><option value="ORACLE">Oracle</option></select></label>
            <label>Host<input value={draft.host} onChange={event => updateDraft('host', event.target.value)} placeholder="db.example.internal" /></label>
            <label>Port<input inputMode="numeric" value={draft.port} onChange={event => updateDraft('port', event.target.value)} placeholder={draft.type === 'ORACLE' ? '1521' : '3306'} /></label>
            <label>Database<input value={draft.database} onChange={event => updateDraft('database', event.target.value)} placeholder={draft.type === 'ORACLE' ? 'FREEPDB1' : 'sales'} /></label>
            <label>Username<input autoComplete="username" value={draft.username} onChange={event => updateDraft('username', event.target.value)} placeholder="report_reader" /></label>
            <label>Password<input aria-label="Password" type="password" autoComplete="new-password" value={draft.password} onChange={event => updateDraft('password', event.target.value)} placeholder={dialog.mode === 'edit' ? '留空表示保留原密码' : '请输入数据库密码'} /><small>{dialog.mode === 'edit' ? '密码不会回显；仅在需要更换时填写。' : '密码由服务端加密保存，不使用 JDBC 连接串。'}</small></label>
          </div>
          {formError && <div className="data-source-feedback error" role="alert">{formError}</div>}
          <footer><button className="quiet-button" type="button" disabled={actionBusy} onClick={closeDialog}>取消</button><button className="primary-button" type="submit" disabled={actionBusy}>{actionBusy ? '正在保存…' : dialog.mode === 'edit' ? '保存修改' : '创建数据源'}</button></footer>
        </form>
      </Dialog>}

      {dialog?.mode === 'view' && dialog.source && <Dialog title="数据表资产" wide onClose={closeDialog}>
        <div className="data-source-detail">
          <div className="data-source-detail-actions" aria-label="表资产操作">
            <button className="action-add-table" type="button" disabled={actionBusy || dialog.source.status !== 'ACTIVE'} onClick={() => void openTableSelection(dialog.source!)}>新增数据表</button>
            <button className="action-refresh-all" type="button" disabled={actionBusy || dialog.source.status !== 'ACTIVE'} onClick={() => void refreshAllTableAssets(dialog.source!)}>{busyAction === `refresh-tables:${dialog.source.id}` ? '刷新加工中…' : '刷新全部元数据'}</button>
          </div>
          <section className="data-source-structure" aria-label="表结构">
            <header><div><span className="eyebrow">元数据结构</span><h3>表与字段</h3></div><strong>{metadataTables.length}<small> 张表</small></strong></header>
            {metadataLoading ? <div className="data-source-structure-state" role="status">正在加载表结构…</div>
              : metadataError ? <div className="data-source-structure-state error" role="alert">{metadataError}<button type="button" onClick={() => void loadTableStructures(dialog.source!.id)}>重新加载</button></div>
              : metadataTables.length === 0 ? <div className="data-source-structure-state">暂无经 LLM 完善的数据表资产，请点击“新增数据表”从源库选择。</div>
              : <div className="data-source-table-list">{metadataTables.map(table => <details key={table.id} onToggle={event => { if (event.currentTarget.open) void loadColumns(table) }}>
                  <summary><span><strong>{table.businessName || table.tableName}</strong><small>{[table.catalogName, table.schemaName, table.tableName].filter(Boolean).join('.')}</small></span><span><em className={`table-management-status ${table.managementStatus.toLowerCase()}`}>{table.managementStatus === 'DISABLED' ? '已停用' : '可用'}</em>{table.tableType || 'TABLE'} · {table.columnCount} 字段</span></summary>
                  <div className="data-source-table-actions" aria-label={`${table.businessName || table.tableName}操作`}>
                    <button className="action-edit" type="button" disabled={actionBusy} onClick={() => openTableEditor(dialog.source!, table)}>修改</button>
                    <button className="action-refresh" type="button" disabled={actionBusy || dialog.source!.status !== 'ACTIVE'} onClick={() => void refreshTableAsset(dialog.source!, table)}>{busyAction === `refresh-table:${table.id}` ? '刷新中…' : '刷新结构'}</button>
                    <button className={table.managementStatus === 'DISABLED' ? 'action-resume' : 'action-pause'} type="button" disabled={actionBusy} onClick={() => void changeTableStatus(dialog.source!, table)}>{table.managementStatus === 'DISABLED' ? '恢复' : '停用'}</button>
                    <button className="action-delete" type="button" disabled={actionBusy} onClick={() => { setFormError(''); setDialog({ mode: 'delete-table', source: dialog.source!, table }) }}>删除</button>
                  </div>
                  {columnLoading[table.id] ? <div className="data-source-column-state">正在加载字段…</div>
                    : metadataColumns[table.id] ? <div className="data-source-column-scroll"><table><thead><tr><th>#</th><th>字段</th><th>原始类型</th><th>标准类型</th><th>可空</th></tr></thead><tbody>{metadataColumns[table.id].map(column => <tr key={column.id}><td>{column.ordinalPosition}</td><td><strong>{column.businessName || column.columnName}</strong>{column.businessName && <small>{column.columnName}</small>}</td><td>{column.nativeType || '—'}</td><td>{column.canonicalType || '—'}</td><td>{column.nullable ? '是' : '否'}</td></tr>)}</tbody></table></div>
                    : null}
                </details>)}</div>}
          </section>
          <footer><button className="quiet-button" type="button" onClick={closeDialog}>关闭</button></footer>
        </div>
      </Dialog>}

      {dialog?.mode === 'select-tables' && dialog.source && <Dialog title="新增数据表" wide onClose={closeDialog}>
        <div className="data-source-table-picker">
          <header><div><strong>{dialog.source.name}</strong><span>从源库选择需要完善并纳入管理的数据表</span></div><small>选中后采集表结构与 3 行样本，经 LLM 完善后存入 PostgreSQL。</small></header>
          {formError && <div className="data-source-feedback error" role="alert">{formError}</div>}
          {discoveryLoading ? <div className="data-source-structure-state" role="status">正在从数据源刷新表清单…</div> : <>
            <div className="data-source-table-picker-toolbar">
              <label><input type="checkbox" checked={discoveredTables.filter(table => !metadataTables.some(asset => assetTableKey(asset) === discoveredTableKey(table))).length > 0 && selectedTableKeys.length === discoveredTables.filter(table => !metadataTables.some(asset => assetTableKey(asset) === discoveredTableKey(table))).length} onChange={event => {
                if (event.target.checked) setSelectedTableKeys(discoveredTables.filter(table => !metadataTables.some(asset => assetTableKey(asset) === discoveredTableKey(table))).map(discoveredTableKey))
                else setSelectedTableKeys([])
              }} />全选可新增表</label><span>已选择 {selectedTableKeys.length} / {discoveredTables.length}</span>
            </div>
            <div className="data-source-discovery-list">{discoveredTables.map(table => {
              const key = discoveredTableKey(table)
              const imported = metadataTables.some(asset => assetTableKey(asset) === key)
              return <label className={imported ? 'imported' : ''} key={key}><input type="checkbox" disabled={imported || actionBusy} checked={selectedTableKeys.includes(key)} onChange={() => setSelectedTableKeys(current => current.includes(key) ? current.filter(item => item !== key) : [...current, key])} /><span><strong>{table.name}</strong><small>{[table.catalogName, table.schemaName, table.name].filter(Boolean).join('.')} · {table.columns.length} 字段</small></span><em>{imported ? '已入库' : table.type}</em></label>
            })}</div>
          </>}
          <footer><button className="quiet-button" type="button" disabled={actionBusy} onClick={() => { setDialog({ mode: 'view', source: dialog.source }); void loadTableStructures(dialog.source!.id) }}>取消</button><button className="primary-button" type="button" disabled={actionBusy || discoveryLoading || selectedTableKeys.length === 0} onClick={() => void importSelectedTables()}>{actionBusy ? '正在采样并完善…' : `新增 ${selectedTableKeys.length} 张表`}</button></footer>
        </div>
      </Dialog>}

      {dialog?.mode === 'edit-table' && dialog.source && dialog.table && <Dialog title="修改数据表资产" onClose={closeDialog}>
        <form className="data-source-form" onSubmit={updateTableAsset}>
          <label>业务名称<input value={tableDraft.businessName} onChange={event => setTableDraft(current => ({ ...current, businessName: event.target.value }))} /></label>
          <label>业务说明<textarea rows={4} value={tableDraft.businessDescription} onChange={event => setTableDraft(current => ({ ...current, businessDescription: event.target.value }))} /></label>
          <label>标签<input value={tableDraft.tags} onChange={event => setTableDraft(current => ({ ...current, tags: event.target.value }))} placeholder="多个标签使用英文逗号分隔" /></label>
          <div className="data-source-form-grid"><label>敏感级别<select value={tableDraft.sensitivityLevel} onChange={event => setTableDraft(current => ({ ...current, sensitivityLevel: event.target.value }))}><option value="PUBLIC">公开</option><option value="INTERNAL">内部</option><option value="CONFIDENTIAL">机密</option><option value="RESTRICTED">严格限制</option></select></label><label>可见范围<select value={tableDraft.visibility} onChange={event => setTableDraft(current => ({ ...current, visibility: event.target.value }))}><option value="PRIVATE">私有</option><option value="TENANT_PUBLIC">租户公开</option></select></label></div>
          <label className="data-source-checkbox"><input type="checkbox" checked={tableDraft.manualLocked} onChange={event => setTableDraft(current => ({ ...current, manualLocked: event.target.checked }))} />锁定人工修改，后续 LLM 刷新不自动覆盖</label>
          {formError && <div className="data-source-feedback error" role="alert">{formError}</div>}
          <footer><button className="quiet-button" type="button" disabled={actionBusy} onClick={() => { setDialog({ mode: 'view', source: dialog.source }); void loadTableStructures(dialog.source!.id) }}>取消</button><button className="primary-button" type="submit" disabled={actionBusy}>{actionBusy ? '正在保存…' : '保存修改'}</button></footer>
        </form>
      </Dialog>}

      {dialog?.mode === 'delete-table' && dialog.source && dialog.table && <Dialog title="删除数据表资产" onClose={closeDialog}>
        <div className="data-source-delete"><p>确认从 PostgreSQL 删除表资产“<strong>{dialog.table.businessName || dialog.table.tableName}</strong>”吗？</p><p className="data-source-safe-note">该操作不会删除或修改源数据库中的原表，之后仍可通过“新增数据表”重新纳入。</p>{formError && <div className="data-source-feedback error" role="alert">{formError}</div>}<footer><button className="quiet-button" type="button" disabled={actionBusy} onClick={() => { setDialog({ mode: 'view', source: dialog.source }); void loadTableStructures(dialog.source!.id) }}>取消</button><button className="data-source-delete-button" type="button" disabled={actionBusy} onClick={() => void deleteTableAsset()}>{actionBusy ? '正在删除…' : '确认删除资产'}</button></footer></div>
      </Dialog>}

      {dialog?.mode === 'delete' && dialog.source && <Dialog title="删除数据源" onClose={closeDialog}>
        <div className="data-source-delete"><p>确认删除“<strong>{dialog.source.name}</strong>”吗？该操作会关闭连接池并从数据源清单移除。</p>{formError && <div className="data-source-feedback error" role="alert">{formError}</div>}<footer><button className="quiet-button" type="button" disabled={actionBusy} onClick={closeDialog}>取消</button><button className="data-source-delete-button" type="button" disabled={actionBusy} onClick={() => void deleteSource()}>{actionBusy ? '正在删除…' : '确认删除'}</button></footer></div>
      </Dialog>}
    </AppShell>
  )
}

function Dialog({ title, children, wide = false, onClose }: { title: string; children: ReactNode; wide?: boolean; onClose: () => void }) {
  return <div className="data-source-dialog-backdrop" role="presentation" onMouseDown={event => { if (event.target === event.currentTarget) onClose() }}><section className={`data-source-dialog${wide ? ' wide' : ''}`} role="dialog" aria-modal="true" aria-labelledby="data-source-dialog-title"><header><div><span className="eyebrow">数据源配置</span><h2 id="data-source-dialog-title">{title}</h2></div><button type="button" aria-label={`关闭${title}`} onClick={onClose}>×</button></header>{children}</section></div>
}
