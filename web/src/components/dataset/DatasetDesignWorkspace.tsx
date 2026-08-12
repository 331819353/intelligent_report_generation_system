import { forwardRef, useMemo, useState, type DragEvent, type ReactNode } from 'react'
import { RowsIcon } from '@phosphor-icons/react'
import type { AssetTable, DesignerNode } from '../../lib/datasets'

export type DatasetAssetSourceGroup = {
  id: string
  name: string
  type: string
  tables: AssetTable[]
  datasetTables: AssetTable[]
  physicalSourceGroups: Array<{
    id: string
    name: string
    type: string
    tables: AssetTable[]
  }>
}

type DatasetAssetSidebarProps = {
  loading: boolean
  groups: DatasetAssetSourceGroup[]
  nodes: DesignerNode[]
  onSelectTable: (table: AssetTable) => void
}

function DatasetAssetSidebar({ loading, groups, nodes, onSelectTable }: DatasetAssetSidebarProps) {
  const [query, setQuery] = useState('')
  const normalizedQuery = query.trim().toLocaleLowerCase()
  const instanceCountByTableID = useMemo(() => nodes.reduce<Record<string, number>>((counts, node) => {
    counts[node.table.id] = (counts[node.table.id] ?? 0) + 1
    return counts
  }, {}), [nodes])
  const matches = (table: AssetTable) => !normalizedQuery || [
    table.businessName,
    table.tableName,
    table.schemaName,
    table.dataSourceName,
  ].some(value => value?.toLocaleLowerCase().includes(normalizedQuery))
  const visibleCount = groups.reduce((total, group) => total +
    group.physicalSourceGroups.reduce((subtotal, source) => subtotal + source.tables.filter(matches).length, 0) +
    group.datasetTables.filter(matches).length, 0)

  const sourceButton = (table: AssetTable, detail: string) => {
    const instanceCount = instanceCountByTableID[table.id] ?? 0
    return <button
      className="dataset-flat-source-item"
      key={table.id}
      type="button"
      draggable
      onDragStart={event => {
        event.dataTransfer.effectAllowed = 'copy'
        event.dataTransfer.setData('text/dataset-table-id', table.id)
      }}
      onClick={() => onSelectTable(table)}
    >
      <span aria-hidden="true"><RowsIcon size={15} weight="duotone" /></span>
      <span>
        <strong>{table.businessName || table.tableName}</strong>
        <small>{detail}</small>
      </span>
      {instanceCount > 0 && <em>已引用 {instanceCount} 次</em>}
    </button>
  }

  return <aside className="dataset-template-tree" aria-label="数据集清单">
    <header>
      <div><strong>数据集清单</strong><small>纵向平铺 · 点击或拖入画布</small></div>
      <span>{visibleCount}</span>
    </header>
    <label className="dataset-source-search">
      <span>搜索</span>
      <input
        type="search"
        value={query}
        onChange={event => setQuery(event.target.value)}
        placeholder="搜索数据集、表名或数据源"
        aria-label="搜索画布数据集"
      />
    </label>
    {loading ? <p className="dataset-source-loading">正在加载数据资产…</p> : <div className="dataset-flat-source-list">
      {groups.map(group => {
        const physicalSources = group.physicalSourceGroups.map(source => ({
          ...source,
          tables: source.tables.filter(matches),
        })).filter(source => source.tables.length)
        const datasetTables = group.datasetTables.filter(matches)
        const layerCount = physicalSources.reduce((total, source) => total + source.tables.length, 0) + datasetTables.length
        if (normalizedQuery && !layerCount) return null
        return <section className="dataset-flat-layer" key={group.id} aria-label={group.name}>
          <header>
            <div><strong>{group.name}</strong><small>{group.type}</small></div>
            <span>{layerCount}</span>
          </header>
          {!layerCount && <p className="source-tree-empty">暂无可用数据集</p>}
          {physicalSources.flatMap(source => source.tables.map(table => sourceButton(
            table,
            `${source.name} · ${table.schemaName}.${table.tableName} · ${table.columnCount} 字段`,
          )))}
          {datasetTables.map(table => sourceButton(
            table,
            `已发布版本 · ${table.tableName} · ${table.columnCount} 字段`,
          ))}
        </section>
      })}
      {normalizedQuery && !visibleCount && <p className="dataset-source-no-results">没有匹配的数据集</p>}
    </div>}
  </aside>
}

type DatasetDesignWorkspaceProps = {
  loading: boolean
  groups: DatasetAssetSourceGroup[]
  tables: AssetTable[]
  nodes: DesignerNode[]
  isFullscreen: boolean
  relationCount: number
  groupCount: number
  transformCount: number
  hasEnd: boolean
  notice: string
  assistant: ReactNode
  canvas: ReactNode
  panels: ReactNode
  feedback?: ReactNode
  onCanvasClick: () => void
  onSelectTable: (table: AssetTable) => void
}

export const DatasetDesignWorkspace = forwardRef<HTMLElement, DatasetDesignWorkspaceProps>(function DatasetDesignWorkspace({
  loading,
  groups,
  tables,
  nodes,
  isFullscreen,
  relationCount,
  groupCount,
  transformCount,
  hasEnd,
  notice,
  assistant,
  canvas,
  panels,
  feedback,
  onCanvasClick,
  onSelectTable,
}, ref) {
  const dropTable = (event: DragEvent<HTMLElement>) => {
    event.preventDefault()
    const tableID = event.dataTransfer.getData('text/dataset-table-id')
    const table = tables.find(item => item.id === tableID)
    if (table) onSelectTable(table)
  }

  return <div className="dataset-create-layout">
    <DatasetAssetSidebar loading={loading} groups={groups} nodes={nodes} onSelectTable={onSelectTable} />
    <main
      ref={ref}
      className={`dataset-template-canvas ${isFullscreen ? 'is-fullscreen' : ''}`}
      aria-label="数据集画布工作区"
      onClick={onCanvasClick}
      onDragOver={event => event.preventDefault()}
      onDrop={dropTable}
    >
      {assistant}
      <section className={`dataset-node-graph ${nodes.length ? '' : 'is-empty'}`} aria-label="组件关系画布">
        <header className="dataset-graph-heading">
          <div>
            <strong>组件关系画布</strong>
            <small>{nodes.length} 个数据节点 · {relationCount} 个关联 · {groupCount} 个分组 · {hasEnd ? '1 个结束节点' : '尚无结束节点'} · {transformCount} 个字段处理</small>
          </div>
          <span>{notice || '拖入组件并连线，结束节点定义最终产物'}</span>
        </header>
        {canvas}
      </section>
      {panels}
      {feedback}
    </main>
  </div>
})
