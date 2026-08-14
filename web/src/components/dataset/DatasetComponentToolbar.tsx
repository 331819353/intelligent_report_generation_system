import { CheckCircleIcon, GitMergeIcon, RowsIcon, type Icon } from '@phosphor-icons/react'
import type { DragEvent } from 'react'
import type { GraphTransformComponentType } from '../../lib/dataset-graph'

export type DatasetToolbarCategory = {
  category: string
  label: string
  className: string
}

export type DatasetToolbarComponent = {
  category: string
  componentType: GraphTransformComponentType
  label: string
  description: string
  sortKey: string
  icon: Icon
}

type DatasetComponentToolbarProps = {
  categories: DatasetToolbarCategory[]
  components: DatasetToolbarComponent[]
  hasEnd: boolean
  onAddJoin: () => void
  onAddGroup: () => void
  onAddTransform: (componentType: GraphTransformComponentType) => void
  onAddEnd: () => void
}

const startDrag = (event: DragEvent<HTMLButtonElement>, value: string) => {
  event.dataTransfer.effectAllowed = 'copy'
  event.dataTransfer.setData('text/dataset-component', value)
}

export function DatasetComponentToolbar({
  categories,
  components,
  hasEnd,
  onAddJoin,
  onAddGroup,
  onAddTransform,
  onAddEnd,
}: DatasetComponentToolbarProps) {
  return <aside className="dataset-component-toolbar" aria-label="画布组件栏">
    <div className="dataset-component-toolbar-heading">
      <strong>组件库</strong>
      <small>选择或拖入画布</small>
    </div>
    <div className="dataset-component-palette">
      <section className="dataset-component-palette-group component-flow" aria-label="流程组件">
        <header><strong>流程组件</strong></header>
        <button type="button" draggable onDragStart={event => startDrag(event, 'GROUP')} onClick={onAddGroup}>
          <RowsIcon data-component-icon="GROUP" aria-hidden="true" size={18} weight="bold" />
          <strong>分组组件</strong><small>可添加多个 / 分组聚合</small>
        </button>
        <button type="button" draggable onDragStart={event => startDrag(event, 'JOIN')} onClick={onAddJoin}>
          <GitMergeIcon data-component-icon="JOIN" aria-hidden="true" size={18} weight="bold" />
          <strong>关联组件</strong><small>双输入 / 可继续连接</small>
        </button>
        <button type="button" draggable={!hasEnd} disabled={hasEnd} onDragStart={event => startDrag(event, 'END')} onClick={onAddEnd}>
          <CheckCircleIcon data-component-icon="END" aria-hidden="true" size={18} weight="bold" />
          <strong>结束节点</strong><small>唯一 / 定义最终输出</small>
        </button>
      </section>
      {categories.map(category => <section key={category.category} className={`dataset-component-palette-group ${category.className}`} aria-label={category.label}>
        <header><strong>{category.label}</strong></header>
        {components
          .filter(item => item.category === category.category)
          .sort((left, right) => left.sortKey.localeCompare(right.sortKey, 'en'))
          .map(item => {
            const ComponentIcon = item.icon
            return <button
              key={item.componentType}
              type="button"
              draggable
              onDragStart={event => startDrag(event, `TRANSFORM:${item.componentType}`)}
              onClick={() => onAddTransform(item.componentType)}
            >
              <ComponentIcon data-component-icon={item.componentType} aria-hidden="true" size={18} weight="bold" />
              <strong>{item.label}</strong>
              <small>{item.description}</small>
            </button>
          })}
      </section>)}
    </div>
  </aside>
}
